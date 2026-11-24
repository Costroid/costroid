// Package gatewaytest serves the LiteLLM proxy routes costroid gateway calls,
// as LiteLLM v1.108.1 answered them on 2026-11-07, for tests. It holds keys,
// teams and users in memory and takes only its own admin key on the admin
// routes.
package gatewaytest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Key is a key as the stub registered it. BudgetDuration is a budget window,
// which mint never sets.
type Key struct {
	Key            string            `json:"key"`
	Alias          string            `json:"key_alias"`
	MaxBudget      json.Number       `json:"max_budget"`
	Duration       string            `json:"duration"`
	MaxParallel    int               `json:"max_parallel_requests"`
	Metadata       map[string]string `json:"metadata"`
	Team           string            `json:"team_id"`
	User           string            `json:"user_id"`
	BudgetDuration string            `json:"budget_duration"`
}

// Budget is a team's or a user's budget.
type Budget struct {
	MaxBudget json.Number `json:"max_budget"`
	Duration  string      `json:"budget_duration"`
}

// LiteLLM is the stub. BeforeDelete, when set, runs before each call to
// /key/delete with the call's number, counted from 1, and BeforeList the same
// before each call to /key/list.
type LiteLLM struct {
	URL          string
	BeforeDelete func(call int)
	BeforeList   func(call int)

	admin   string
	mu      sync.Mutex
	keys    map[string]Key
	expires map[string]time.Time
	spend   map[string]string
	budgets map[string]Budget
	deletes int
	lists   int
	refused int
	// generated counts the calls to /key/generate, and budgetWrites those
	// that create or set a team's or a user's budget.
	generated    int
	budgetWrites int
}

// New starts the stub for the admin key given and closes it when the test ends.
func New(t testing.TB, admin string) *LiteLLM {
	t.Helper()
	l := &LiteLLM{admin: admin, keys: map[string]Key{}, expires: map[string]time.Time{}, spend: map[string]string{}, budgets: map[string]Budget{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /key/generate", l.adminOnly(l.generate))
	mux.HandleFunc("GET /key/info", l.info)
	mux.HandleFunc("GET /key/list", l.adminOnly(l.list))
	mux.HandleFunc("POST /key/delete", l.adminOnly(l.delete))
	for _, kind := range []string{"team", "user"} {
		mux.HandleFunc("GET /"+kind+"/info", l.adminOnly(l.budgetInfo(kind)))
		mux.HandleFunc("POST /"+kind+"/new", l.adminOnly(l.budgetSet(kind)))
		mux.HandleFunc("POST /"+kind+"/update", l.adminOnly(l.budgetSet(kind)))
	}
	mux.HandleFunc("GET /health/readiness", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"healthy","db":"connected"}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	l.URL = server.URL
	return l
}

// Add registers a key as if a mint had. A key with a duration expires that
// long after it was added, as LiteLLM sets expires from duration.
func (l *LiteLLM) Add(key Key) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys[key.Alias] = key
	if lifetime, err := time.ParseDuration(key.Duration); err == nil {
		l.expires[key.Alias] = time.Now().Add(lifetime)
	}
}

// SetSpend sets what the stub counted on the key with the alias, as the JSON
// number LiteLLM would write. A key's spend is 0.0 until it is set.
func (l *LiteLLM) SetSpend(alias, spend string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.spend[alias] = spend
}

func (l *LiteLLM) spendOf(alias string) json.Number {
	if spend, ok := l.spend[alias]; ok {
		return json.Number(spend)
	}
	return json.Number("0.0")
}

// Key returns the key with the alias.
func (l *LiteLLM) Key(alias string) (Key, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key, ok := l.keys[alias]
	return key, ok
}

// Budget returns the team's or the user's budget: kind is team or user.
func (l *LiteLLM) Budget(kind, id string) (Budget, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	budget, ok := l.budgets[kind+":"+id]
	return budget, ok
}

// Registrations counts the calls to /key/generate, and BudgetWrites those
// that create or set a team's or a user's budget.
func (l *LiteLLM) Registrations() (generated, budgetWrites int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.generated, l.budgetWrites
}

// Refused counts the calls made with any bearer but the admin key or a key
// the stub holds.
func (l *LiteLLM) Refused() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.refused
}

func (l *LiteLLM) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+l.admin {
			l.refuse(w)
			return
		}
		next(w, r)
	}
}

func (l *LiteLLM) refuse(w http.ResponseWriter) {
	l.mu.Lock()
	l.refused++
	l.mu.Unlock()
	answer(w, http.StatusUnauthorized, `{"error":{"message":"Authentication Error, Invalid API key provided.","code":"401"}}`)
}

func (l *LiteLLM) generate(w http.ResponseWriter, r *http.Request) {
	var key Key
	if err := json.NewDecoder(r.Body).Decode(&key); err != nil || len(key.Key) < 16 {
		answer(w, http.StatusBadRequest, `{"error":{"message":"Invalid key format.","code":"400"}}`)
		return
	}
	l.mu.Lock()
	l.generated++
	l.mu.Unlock()
	l.Add(key)
	answer(w, http.StatusOK, `{}`)
}

// info answers for the key named by id to the admin, or for the bearer's own key.
func (l *LiteLLM) info(w http.ResponseWriter, r *http.Request) {
	bearer := r.Header.Get("Authorization")
	l.mu.Lock()
	var found *Key
	for _, key := range l.keys {
		if (bearer == "Bearer "+l.admin && keyID(key.Key) == r.URL.Query().Get("key")) || bearer == "Bearer "+key.Key {
			found = &key
			break
		}
	}
	l.mu.Unlock()
	if found == nil {
		if bearer == "Bearer "+l.admin {
			answer(w, http.StatusNotFound, `{"error":{"message":"Key not found in database","code":"404"}}`)
			return
		}
		l.refuse(w)
		return
	}
	l.mu.Lock()
	body, _ := json.Marshal(map[string]any{"info": l.object(*found)})
	l.mu.Unlock()
	answer(w, http.StatusOK, string(body))
}

// object is the key as LiteLLM states it in full: its id in token, its alias,
// its spend, its limits, its metadata, its team, its user, its expiry and its
// budget window. A key added with no duration has no expiry.
func (l *LiteLLM) object(key Key) map[string]any {
	expires, window := any(nil), any(nil)
	if at, ok := l.expires[key.Alias]; ok {
		expires = at.UTC().Format("2006-01-02T15:04:05.000000+00:00")
	}
	if key.BudgetDuration != "" {
		window = key.BudgetDuration
	}
	return map[string]any{
		"token": keyID(key.Key), "key_alias": key.Alias, "spend": l.spendOf(key.Alias),
		"max_budget": key.MaxBudget, "max_parallel_requests": key.MaxParallel, "metadata": key.Metadata,
		"team_id": key.Team, "user_id": key.User, "expires": expires, "budget_duration": window,
	}
}

// list answers the keys with the alias or the id the query names, exactly,
// as full objects. Only a list by alias, the one close reads the spend
// through, counts as a call to BeforeList.
func (l *LiteLLM) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	hash := query.Get("key_hash")
	if hash == "" {
		l.mu.Lock()
		l.lists++
		call := l.lists
		l.mu.Unlock()
		if l.BeforeList != nil {
			l.BeforeList(call)
		}
	}
	keys := []map[string]any{}
	l.mu.Lock()
	for alias, key := range l.keys {
		named := hash == "" && alias == query.Get("key_alias") || hash != "" && keyID(key.Key) == hash
		if named && query.Get("return_full_object") == "true" {
			keys = append(keys, l.object(key))
		}
	}
	l.mu.Unlock()
	body, _ := json.Marshal(map[string]any{"keys": keys, "total_count": len(keys), "current_page": 1, "total_pages": 1})
	answer(w, http.StatusOK, string(body))
}

func (l *LiteLLM) delete(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	l.deletes++
	call := l.deletes
	l.mu.Unlock()
	if l.BeforeDelete != nil {
		l.BeforeDelete(call)
	}
	var body struct {
		Aliases []string `json:"key_aliases"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	l.mu.Lock()
	var deleted []string
	for _, alias := range body.Aliases {
		if _, ok := l.keys[alias]; ok {
			delete(l.keys, alias)
			deleted = append(deleted, alias)
		}
	}
	l.mu.Unlock()
	if len(deleted) == 0 {
		answer(w, http.StatusNotFound, `{"error":{"message":"{'error': 'No keys found'}","code":"404"}}`)
		return
	}
	data, _ := json.Marshal(map[string][]string{"deleted_keys": deleted})
	answer(w, http.StatusOK, string(data))
}

func (l *LiteLLM) budgetInfo(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := l.Budget(kind, r.URL.Query().Get(kind+"_id")); !ok {
			answer(w, http.StatusNotFound, `{"error":{"message":"not found","code":"404"}}`)
			return
		}
		answer(w, http.StatusOK, `{}`)
	}
}

func (l *LiteLLM) budgetSet(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			answer(w, http.StatusBadRequest, `{}`)
			return
		}
		var id string
		var budget Budget
		_ = json.Unmarshal(body[kind+"_id"], &id)
		_ = json.Unmarshal(body["max_budget"], &budget.MaxBudget)
		_ = json.Unmarshal(body["budget_duration"], &budget.Duration)
		l.mu.Lock()
		l.budgets[kind+":"+id] = budget
		l.budgetWrites++
		l.mu.Unlock()
		answer(w, http.StatusOK, `{}`)
	}
}

func answer(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write([]byte(body))
}

func keyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

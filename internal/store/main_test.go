package store_test

import (
	"os"
	"testing"

	"github.com/Costroid/costroid/internal/store/storetest"
)

func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

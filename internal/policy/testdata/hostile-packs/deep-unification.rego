# OPA unifies a_20 with b_20 through continuations, so the stack grows with
# the 2^20 pairs it visits, past any limit Go allows a goroutine: the
# process dies of a stack overflow, which Go cannot recover.
package costroid.pack

a_0 := {"rows": input.rows}
a_1 := object.union({"left": a_0}, {"right": a_0})
a_2 := object.union({"left": a_1}, {"right": a_1})
a_3 := object.union({"left": a_2}, {"right": a_2})
a_4 := object.union({"left": a_3}, {"right": a_3})
a_5 := object.union({"left": a_4}, {"right": a_4})
a_6 := object.union({"left": a_5}, {"right": a_5})
a_7 := object.union({"left": a_6}, {"right": a_6})
a_8 := object.union({"left": a_7}, {"right": a_7})
a_9 := object.union({"left": a_8}, {"right": a_8})
a_10 := object.union({"left": a_9}, {"right": a_9})
a_11 := object.union({"left": a_10}, {"right": a_10})
a_12 := object.union({"left": a_11}, {"right": a_11})
a_13 := object.union({"left": a_12}, {"right": a_12})
a_14 := object.union({"left": a_13}, {"right": a_13})
a_15 := object.union({"left": a_14}, {"right": a_14})
a_16 := object.union({"left": a_15}, {"right": a_15})
a_17 := object.union({"left": a_16}, {"right": a_16})
a_18 := object.union({"left": a_17}, {"right": a_17})
a_19 := object.union({"left": a_18}, {"right": a_18})
a_20 := object.union({"left": a_19}, {"right": a_19})

b_0 := {"rows": input.rows}
b_1 := object.union({"left": b_0}, {"right": b_0})
b_2 := object.union({"left": b_1}, {"right": b_1})
b_3 := object.union({"left": b_2}, {"right": b_2})
b_4 := object.union({"left": b_3}, {"right": b_3})
b_5 := object.union({"left": b_4}, {"right": b_4})
b_6 := object.union({"left": b_5}, {"right": b_5})
b_7 := object.union({"left": b_6}, {"right": b_6})
b_8 := object.union({"left": b_7}, {"right": b_7})
b_9 := object.union({"left": b_8}, {"right": b_8})
b_10 := object.union({"left": b_9}, {"right": b_9})
b_11 := object.union({"left": b_10}, {"right": b_10})
b_12 := object.union({"left": b_11}, {"right": b_11})
b_13 := object.union({"left": b_12}, {"right": b_12})
b_14 := object.union({"left": b_13}, {"right": b_13})
b_15 := object.union({"left": b_14}, {"right": b_14})
b_16 := object.union({"left": b_15}, {"right": b_15})
b_17 := object.union({"left": b_16}, {"right": b_16})
b_18 := object.union({"left": b_17}, {"right": b_17})
b_19 := object.union({"left": b_18}, {"right": b_18})
b_20 := object.union({"left": b_19}, {"right": b_19})

results contains {"rule": "team_rule", "outcome": "warn", "reason": "reached"} if {
	a_20 == b_20
}

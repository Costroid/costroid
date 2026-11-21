# Each value holds the one before it twice, so the type checker's work on
# a_14 == b_14 grows four times with each rule: about 12 seconds of OPA
# 1.22.2's compiler.
package costroid.pack

a_0 := [1]
a_1 := [a_0, a_0]
a_2 := [a_1, a_1]
a_3 := [a_2, a_2]
a_4 := [a_3, a_3]
a_5 := [a_4, a_4]
a_6 := [a_5, a_5]
a_7 := [a_6, a_6]
a_8 := [a_7, a_7]
a_9 := [a_8, a_8]
a_10 := [a_9, a_9]
a_11 := [a_10, a_10]
a_12 := [a_11, a_11]
a_13 := [a_12, a_12]
a_14 := [a_13, a_13]

b_0 := [1]
b_1 := [b_0, b_0]
b_2 := [b_1, b_1]
b_3 := [b_2, b_2]
b_4 := [b_3, b_3]
b_5 := [b_4, b_4]
b_6 := [b_5, b_5]
b_7 := [b_6, b_6]
b_8 := [b_7, b_7]
b_9 := [b_8, b_8]
b_10 := [b_9, b_9]
b_11 := [b_10, b_10]
b_12 := [b_11, b_11]
b_13 := [b_12, b_12]
b_14 := [b_13, b_13]

results contains {"rule": "team_rule", "outcome": "warn", "reason": "reached"} if {
	a_14 == b_14
}

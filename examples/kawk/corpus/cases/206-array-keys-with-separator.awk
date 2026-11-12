BEGIN { a["a b"] = 1; a["a\tb"] = 2; a[""] = 3; a[" "] = 4; a["\034"] = 5; print a["a b"], a["a\tb"], a[""], a[" "], length(a), ("", "") in a }

function change(x) { x = x * 2; return x }
BEGIN { y = 5; print change(y), y; s = "a"; print change(s) "|" s }

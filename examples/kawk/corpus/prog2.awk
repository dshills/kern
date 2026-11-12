END { print "second file", NR }
function helper(x) { return x * 2 }
BEGIN { print helper(21) }

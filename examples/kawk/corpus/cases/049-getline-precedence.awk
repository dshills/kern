BEGIN { print "5\n6" > "f"; close("f"); f = "f"
  while (getline line < f > 0) print "a", line
  close(f); getline x < f; print x + 1; r = getline y < f; print r, y
  "echo hi" | getline z; print z }

BEGIN { r = ("exit 3" | getline x); print r; print close("exit 3"); r = ("nosuchcommand_xyz 2>/dev/null" | getline x); print r }

BEGIN { r = system("kill -9 $$"); print r; r = system("kill -TERM $$"); print r; r = system("exit 255"); print r }

BEGIN { s = ""; for (i = 0; i < 2000; i++) s = s "x"; print length(s); t = s s s s; print length(t); print substr(t, 7990) }

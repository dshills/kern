BEGIN { a = "0"; s = sprintf("%d", a); print (a ? "T" : "F"); b = "0"; s = sprintf("%s", b); print (b ? "T" : "F"); c = "0"; s = substr("abc", c); print (c ? "T" : "F")
  d = "0"; s = int(d); print (d ? "T" : "F"); e["k"] = "0"; s = e["k"] + 1; print (e["k"] ? "T" : "F"); $0 = "p q"; $1 = "0"; print ($1 ? "T" : "F"); s = $1 + 0; print ($1 ? "T" : "F")
  f = "0"; s = -f; print (f ? "T" : "F"); g = "0"; if (g < 1) s = 1; print (g ? "T" : "F"); h = "0"; h2 = h; s = h2 + 1; print (h ? "T" : "F"), (h2 ? "T" : "F")
  i = "00"; s = i++; print i, (i ? "T" : "F"); j = "0"; printf "%5.2f|%s\n", j, (j ? "T" : "F"); k = "0"; printf "%c|%s\n", k, (k ? "T" : "F") }

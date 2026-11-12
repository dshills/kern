function add(k, v) { store[k] = v; nkeys++ } function get(k) { return store[k] } BEGIN { add("a", 1); add("b", 2); print get("a") + get("b"), nkeys, length(store) }

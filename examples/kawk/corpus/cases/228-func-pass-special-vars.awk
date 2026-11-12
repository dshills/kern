function show(a, b, c) { return a ":" b ":" c } { if (NR == 2) print show(NF, NR, $0), show($1, $NF, FILENAME == "") }

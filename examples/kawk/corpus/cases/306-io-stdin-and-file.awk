{ n[FILENAME]++ } END { print NR; for (f in n) c++; print c }

/a/ { a++ } /b/ { b++ } /c/ { c++ } /d/ { d++ } /e/ { e++ } /a/ && /b/ { ab++ } /a/ || /b/ { aob++ } !/a/ { na++ } NF == 0 { z++ } NF { nz++ } END { print a, b, c, d, e, ab, aob, na, z, nz, NR }

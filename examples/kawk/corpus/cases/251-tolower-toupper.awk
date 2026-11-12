{ print toupper($1), tolower($NF) } END { print toupper("abc123xyz!"), tolower("ABC123XYZ!"), toupper(""), toupper(1e3), tolower("MiXeD CaSe"), toupper("\351") == "\351" }

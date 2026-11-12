BEGIN { print "[" FILENAME "]" } NR == 1 { print "[" FILENAME "]" } END { print "[" FILENAME "]" }

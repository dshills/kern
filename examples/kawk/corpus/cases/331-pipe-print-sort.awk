{ if (NF) print $1 | "sort" } END { close("sort"); print "after sort" }

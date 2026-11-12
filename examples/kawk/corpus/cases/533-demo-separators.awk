BEGIN {
    FS = ":"; $0 = "root:x:0:0::/root:/bin/sh"; print NF, $1, $6, "[" $5 "]"
    FS = "\t"; $0 = "a b\tc d\t\te"; print NF, $2, "[" $3 "]"
    FS = ", *|;"; $0 = "p, q,r;s"; print NF, $1 $2 $3 $4
    FS = "|"; $0 = "x|y|z"; print NF, $2
    FS = ""; $0 = "kern"; print NF, $4 $3 $2 $1
    FS = " "; RS = ""
}
{ print "paragraph " NR ": " NF " words, from " $1 " to " $NF }
END { RS = ";"; print NR " paragraphs" }

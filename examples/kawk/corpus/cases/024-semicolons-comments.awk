# a comment
BEGIN { x = 1 ; ; y = 2 # trailing comment
;
  print x, y; # another
} # after brace
# end
END { print "done" } ;

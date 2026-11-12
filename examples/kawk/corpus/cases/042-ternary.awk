BEGIN { x = 3; print x > 2 ? "big" : "small" > "/dev/stdout"
  print (x > 2 ? "big" : "small"); print x == 3 ? x == 4 ? "a" : "b" : "c"; y = x < 2 ? 1 : x < 4 ? 2 : 3; print y
  print 1 ? 2 ? "x" : "y" : "z"; print (0 ? "a" : 0 ? "b" : "c") }

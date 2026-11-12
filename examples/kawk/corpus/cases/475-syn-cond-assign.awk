BEGIN { x = 5; y = x > 3 ? "big" : "small"; print y; z = x < 3 ? a = 1 : b = 2; print z, a + 0, b; x > 3 ? c = "yes" : c = "no"; print c }

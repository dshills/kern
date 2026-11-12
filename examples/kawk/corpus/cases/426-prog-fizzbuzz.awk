BEGIN { for (i = 1; i <= 30; i++) print (i % 15 == 0 ? "FizzBuzz" : i % 5 == 0 ? "Buzz" : i % 3 == 0 ? "Fizz" : i) }

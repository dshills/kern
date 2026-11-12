function f(a, b)
{
  return a - b
}
function g ( x ) { return x + 1 }
BEGIN { print f(5, 3), g(1) }

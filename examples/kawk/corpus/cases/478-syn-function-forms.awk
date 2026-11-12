function a() { return 1 }
function b(x) {
  return x + 1
}
function c(x, y)
{ return x * y }
function d (x) { return -x }
BEGIN { print a(), b(1), c(2, 3), d(4), a() + b(a()) }

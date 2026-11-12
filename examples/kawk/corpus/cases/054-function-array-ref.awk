function fill(arr, n,   i) { for (i = 1; i <= n; i++) arr[i] = i * i }
function sum(arr,   k, t) { for (k in arr) t += arr[k]; return t }
function clear(arr,  k) { for (k in arr) delete arr[k] }
BEGIN { fill(sq, 5); print sum(sq), length(sq); clear(sq); print length(sq); fill(other, 3); print other[3] }

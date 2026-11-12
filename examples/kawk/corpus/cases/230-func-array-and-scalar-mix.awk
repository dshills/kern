function setup(arr, n, tag,  i) { for (i = 1; i <= n; i++) arr[i] = tag i; return n } BEGIN { n = setup(A, 3, "a"); m = setup(B, 2, "b"); print n, m, A[1], A[3], B[2], length(A) + length(B) }

function bump() { count++; total += count }
BEGIN { bump(); bump(); bump(); print count, total }

{ if (NF > 3) { big++ }
  else { small++ }
  if (NF == 0) e++
  else
    if (NF == 1) o++
    else
      m++
}
END { print big, small, e, o, m }

BEGIN { print match("foobar", /o+/), RSTART, RLENGTH; print match("foobar", /z/), RSTART, RLENGTH; print match("abc", /$/), RSTART, RLENGTH
  print match("xabcabc", /(abc)+/), RSTART, RLENGTH; print match("the quick", /[a-z]+$/), RSTART, RLENGTH; print match("aaa", /a*?/), RSTART, RLENGTH
  s = "key=value"; if (match(s, /=/)) print substr(s, 1, RSTART - 1), substr(s, RSTART + RLENGTH) }

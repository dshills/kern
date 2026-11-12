{ if (NF > 2 &&
      $1 != "" ||
      NR == 3) n++ }
END { print n }

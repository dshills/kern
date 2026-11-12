{ print NF "|" $0 "|" } !NF { empty++ } END { print empty }

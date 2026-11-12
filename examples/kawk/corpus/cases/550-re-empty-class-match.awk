{ printf "[%s] a=%d b=%d c=%d d=%d e=%d,%d f=%d g=%d,%d\n", $0, /^[z-a]$/, /x[z-a]y/, /^[z-a]/, /[z-a]$/, match($0, /[z-a]/), RLENGTH, /[z-a]+y/, match($0, /[z-a]*m/), RLENGTH }

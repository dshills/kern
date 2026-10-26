# lldb command module for kern (`kern debug`): `kern-own` prints the
# ownership state of every place on the current source line, as reported by
# `kern explain-ownership` for the span of that line.
import json
import os
import subprocess

import lldb


def kern_own(debugger, command, result, internal_dict):
    target = debugger.GetSelectedTarget()
    frame = target.GetProcess().GetSelectedThread().GetSelectedFrame()
    entry = frame.GetLineEntry()
    line = entry.GetLine()
    spec = entry.GetFileSpec()
    name = spec.GetFilename()
    full = os.path.normpath(os.path.join(spec.GetDirectory() or "", name or ""))
    files = [f for f in os.environ.get("KERN_FILES", "").split(os.pathsep) if f]
    match = [f for f in files if os.path.normpath(f) == full]
    if not match:
        match = [f for f in files if os.path.basename(f) == name]
    if not line or not match:
        result.AppendMessage("kern-own: no kern source line at this frame")
        return
    kern = os.environ.get("KERN_BIN", "kern")
    try:
        out = subprocess.run([kern, "explain-ownership", "--line=%d" % line] + match + [f for f in files if f not in match], capture_output=True, text=True)
    except OSError as e:
        result.AppendMessage("kern-own: %s" % e)
        return
    try:
        resp = json.loads(out.stdout)
    except ValueError:
        result.AppendMessage(out.stdout + out.stderr)
        return
    places = (resp.get("result") or {}).get("places") or []
    if not places:
        result.AppendMessage("%s:%d: no places on this line" % (name, line))
    for p in places:
        loc = p.get("loc", {}).get("span", {})
        drop = p.get("drop_at")
        where = "drop at %d-%d" % (drop["span"]["start"], drop["span"]["end"]) if drop else "no drop"
        accesses = ", ".join("%s@%d-%d" % (a["kind"], a["span"]["start"], a["span"]["end"]) for a in p.get("accesses", []))
        result.AppendMessage("%s:%d place %d-%d state=%s %s [%s] (%s)" % (name, line, loc.get("start"), loc.get("end"), p.get("state"), where, accesses, p.get("label")))


def __lldb_init_module(debugger, internal_dict):
    debugger.HandleCommand("command script add -f kern_lldb.kern_own kern-own")
    print("kern: `kern-own` prints the ownership state of the places on the current line")

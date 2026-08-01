' Where this TV points.
'
' Yarr.It is self-hostable: the search service and the streaming gateway both
' run on the viewer's own hardware, so neither address can be a constant. The
' owner configures ONE address, kept in the same registry section as the access
' token under "server_base", and everything else is derived from it.
'
' This lives in its own file because the entry point and the scene both need the
' same answers, and a SceneGraph component cannot see functions that merely
' happen to sit in source/ -- it only sees scripts attached to it. So this file
' is pulled into MainScene.xml explicitly, and calling any of it from a
' component that has not done the same is a runtime error, not a compile one.

' The address shipped with the channel, used by a TV nobody has pointed
' anywhere else.
function defaultServerBase() as String
    return "https://yarrit.com"
end function

' The server this TV is actually pointed at.
'
' Registry values are strings only, so a key that was written and then blanked
' is indistinguishable from one that was never set -- both have to fall back, or
' a bad save would strand the channel with nowhere to talk to.
function serverBase() as String
    reg = CreateObject("roRegistrySection", "yarrit")
    if reg.exists("server_base")
        saved = reg.read("server_base")
        if saved <> invalid and saved.trim() <> "" then return saved.trim()
    end if
    return defaultServerBase()
end function

' The gateway shipped with the channel. Only ever used by a TV that has been
' pointed nowhere, which in practice means the machine this was developed on.
function defaultGatewayBase() as String
    return "http://192.168.0.118:8900"
end function

' The streaming gateway, derived from the configured server rather than stored.
'
' There is no gateway we host: the machine that turns a magnet into a stream is
' whichever one the viewer set up, and in every real deployment that is the same
' host as the search service. Deriving it is what keeps this to ONE address --
' typing a second URL on a D-pad keyboard costs more than it could ever buy, and
' a split pair is one more thing to get out of step.
'
' The scheme is not carried over: the gateway serves raw containers over plain
' HTTP on its own port, so a server reached over HTTPS still has an http://
' gateway beside it.
function derivedGatewayBase() as String
    reg = CreateObject("roRegistrySection", "yarrit")
    if reg.exists("server_base")
        saved = reg.read("server_base")
        if saved <> invalid and saved.trim() <> ""
            host = hostOf(saved.trim())
            if host <> "" then return "http://" + host + ":8900"
        end if
    end if
    return defaultGatewayBase()
end function

' hostOf strips a URL down to its bare hostname.
'
' Deliberately not a general URL parser: it needs to survive what somebody types
' on a television keyboard -- a scheme or none, a port or none, a stray trailing
' path -- and nothing more exotic than that ever reaches it.
function hostOf(url as String) as String
    rest = url
    at = Instr(1, rest, "://")
    if at > 0 then rest = Mid(rest, at + 3)

    slash = Instr(1, rest, "/")
    if slash > 0 then rest = Left(rest, slash - 1)

    colon = Instr(1, rest, ":")
    if colon > 0 then rest = Left(rest, colon - 1)

    return rest
end function

' normalizeServer turns what somebody managed to type into something openable.
'
' A scheme is expensive to type on an on-screen keyboard and a trailing slash is
' easy to leave behind, and either one silently produces a request for
' "host//api/search" or for an address roUrlTransfer refuses outright. A bare
' host is assumed to be plain HTTP, because a self-hosted box on a home LAN
' rarely has a certificate.
function normalizeServer(value as String) as String
    ' Every space goes, not just the ends. A URL cannot contain one, the space
    ' bar on Roku's keyboard sits directly under the letter block where it is
    ' easy to catch by accident, and a space in the middle of an address is
    ' invisible on a television from across a room.
    v = ""
    for i = 1 to Len(value)
        ch = Mid(value, i, 1)
        if ch <> " " and ch <> Chr(9) then v = v + ch
    end for

    if v = "" then return ""
    if Instr(1, LCase(v), "://") = 0 then v = "http://" + v
    while Len(v) > 1 and Right(v, 1) = "/"
        v = Left(v, Len(v) - 1)
    end while
    return v
end function

' Network task: talks to the public search API and to the LAN gateway.

sub init()
    m.top.functionName = "runRequest"
end sub

sub runRequest()
    req = m.top.request
    if req = invalid then return

    global = m.top.getGlobalNode()
    kind = req.kind

    if kind = "discover"
        url = global.searchBase + "/api/discover"
        m.top.response = { kind: kind, ok: true, data: httpGetJson(url, 25) }

    else if kind = "search"
        ' device=roku makes the server drop anything this box cannot decode --
        ' AVI, WMV, XviD and so on. Roku shows a bare "cannot play" error with no
        ' explanation, so a source it will refuse is worse than no source at all.
        ' minSeeders=1 drops dead torrents for the same reason.
        url = global.searchBase + "/api/search?device=roku&minSeeders=1&q=" + urlEncode(req.query)
        m.top.response = { kind: kind, ok: true, data: httpGetJson(url, 90) }

    else if kind = "prepare"
        ' The gateway joins the swarm and waits for metadata, which can take a
        ' while, so this gets a long timeout and its own error surface.
        url = global.gatewayBase + "/prepare?magnet=" + urlEncode(req.magnet)
        result = httpGetJson(url, 140)
        ok = result <> invalid and result.streamUrl <> invalid
        m.top.response = { kind: kind, ok: ok, data: result }
    end if
end sub

' httpGetJson performs a GET and parses JSON, returning invalid on any failure.
function httpGetJson(url as String, timeoutSec as Integer) as Object
    xfer = CreateObject("roUrlTransfer")
    xfer.setUrl(url)
    xfer.setCertificatesFile("common:/certs/ca-bundle.crt")
    xfer.initClientCertificates()
    xfer.addHeader("Accept", "application/json")

    port = CreateObject("roMessagePort")
    xfer.setMessagePort(port)

    if not xfer.asyncGetToString() then return invalid

    msg = wait(timeoutSec * 1000, port)
    if type(msg) <> "roUrlEvent"
        ' Timed out: cancel so the transfer does not linger.
        xfer.asyncCancel()
        return invalid
    end if

    if msg.getResponseCode() <> 200 then return invalid

    parsed = ParseJson(msg.getString())
    return parsed
end function

' urlEncode escapes a value for use in a query string. roUrlTransfer.escape()
' handles this, but it needs an instance, so one is kept around.
function urlEncode(value as String) as String
    if m.escaper = invalid then m.escaper = CreateObject("roUrlTransfer")
    return m.escaper.escape(value)
end function

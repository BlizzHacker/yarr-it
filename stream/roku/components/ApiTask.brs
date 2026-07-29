' Network task: talks to the public search API and to the LAN gateway.

sub init()
    m.top.functionName = "runRequest"
end sub

sub runRequest()
    req = m.top.request
    if req = invalid then return

    ' Inside a component the global node is m.global. getGlobalNode() only
    ' exists on roSGScreen, so calling it here is a member-not-found error.
    globalNode = m.global
    kind = req.kind

    if kind = "discover"
        url = globalNode.searchBase + "/api/discover"
        m.top.response = { kind: kind, ok: true, data: httpGetJson(url, 25) }

    else if kind = "search"
        ' device=roku makes the server drop anything this box cannot decode --
        ' AVI, WMV, XviD and so on. Roku shows a bare "cannot play" error with no
        ' explanation, so a source it will refuse is worse than no source at all.
        url = globalNode.searchBase + "/api/search?device=roku"

        ' An empty query is a browse: "show me comics". The server accepts that
        ' as long as a kind or group says what to browse.
        if req.query <> invalid and req.query <> ""
            url = url + "&q=" + urlEncode(req.query)
        end if

        ' Category. `kind` narrows to what a thing IS (image, comic); `groups`
        ' narrows by catalogue section. Movies needs the group, because a film
        ' and a TV episode are both kind=video.
        if req.filterKind <> invalid and req.filterKind <> ""
            url = url + "&kind=" + urlEncode(req.filterKind)
        end if
        if req.filterGroups <> invalid and req.filterGroups <> ""
            url = url + "&groups=" + urlEncode(req.filterGroups)
        end if

        ' Ranking. Sorting by seeders is what makes the first row the one most
        ' likely to actually play, which matters far more on a TV than in a
        ' browser: there is no second window to go and check another candidate.
        sortBy = "seeders"
        if req.sort <> invalid and req.sort <> "" then sortBy = req.sort
        url = url + "&sort=" + urlEncode(sortBy)

        ' Dead torrents waste the whole minute the gateway spends waiting for
        ' metadata before failing. Stills have no seeders at all, so the floor
        ' only applies where it means something.
        if req.filterKind <> "image" and req.filterKind <> "comic"
            url = url + "&minSeeders=1"
        end if

        m.top.response = { kind: kind, ok: true, data: httpGetJson(url, 150) }

    else if kind = "prepare"
        ' The gateway joins the swarm and waits for metadata, which can take a
        ' while, so this gets a long timeout and its own error surface.
        url = globalNode.gatewayBase + "/prepare?magnet=" + urlEncode(req.magnet)
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

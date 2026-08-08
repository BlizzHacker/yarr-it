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
        m.top.response = { kind: kind, ok: true, data: httpGetJson(url, 60) }

    else if kind = "search"
        ' device=roku makes the server drop anything this box cannot decode --
        ' AVI, WMV, XviD and so on. Roku shows a bare "cannot play" error with no
        ' explanation, so a source it will refuse is worse than no source at all.
        ' Built once and used for both the search and the collection of its
        ' remainder. The filters have to be identical on both, or the settled
        ' result is a differently-filtered, differently-sorted set that replaces
        ' the first one -- which reads as the results changing their minds.
        qs = "device=roku"

        ' An empty query is a browse: "show me comics". The server accepts that
        ' as long as a kind or group says what to browse.
        if req.query <> invalid and req.query <> ""
            qs = qs + "&q=" + urlEncode(req.query)
        end if

        ' Category. `kind` narrows to what a thing IS (image, comic); `groups`
        ' narrows by catalogue section. Movies needs the group, because a film
        ' and a TV episode are both kind=video.
        if req.filterKind <> invalid and req.filterKind <> ""
            qs = qs + "&kind=" + urlEncode(req.filterKind)
        end if
        if req.filterGroups <> invalid and req.filterGroups <> ""
            qs = qs + "&groups=" + urlEncode(req.filterGroups)
        end if

        ' Ranking. Sorting by seeders is what makes the first row the one most
        ' likely to actually play, which matters far more on a TV than in a
        ' browser: there is no second window to go and check another candidate.
        sortBy = "seeders"
        if req.sort <> invalid and req.sort <> "" then sortBy = req.sort
        qs = qs + "&sort=" + urlEncode(sortBy)

        ' Dead torrents waste the whole minute the gateway spends waiting for
        ' metadata before failing. Stills have no seeders at all, so the floor
        ' only applies where it means something.
        if req.filterKind <> "image" and req.filterKind <> "comic"
            qs = qs + "&minSeeders=1"
        end if

        url = globalNode.searchBase + "/api/search?" + qs

        ' The server answers within a few hundred milliseconds with whatever it
        ' already had -- its cache and archive.org -- and finishes the torrent
        ' fan-out behind a job id. This box waited for the whole thing before:
        ' measured against the live server, 23 to 40 seconds, and 112 seconds
        ' ending in an error whenever Prowlarr stalled.
        '
        ' The rest is collected with the same plain GET this file already does.
        ' That is why the server offers a job id rather than an event stream:
        ' roUrlTransfer cannot read a stream, and cannot parse a body it has
        ' not finished receiving.
        first = httpGetJson(url, 30)
        m.top.response = { kind: kind, ok: true, data: first }

        ' Deliberately two repaints and no more: the first, at once, and one
        ' settled result. A TV grid redraws from the top, so publishing every
        ' arrival would move the tile somebody is pointing the remote at.
        final = collectSearch(globalNode, first, qs)
        if final <> invalid
            m.top.response = { kind: kind, ok: true, data: final }
        end if

    else if kind = "deviceStart"
        ' RFC 8628. The TV asks for a code; the person approves it on a phone.
        ' No secret is involved because a client secret shipped inside a TV
        ' binary is not a secret.
        body = "client_id=" + urlEncode(globalNode.tvClientId) + "&scope=" + urlEncode("openid profile email")
        m.top.response = { kind: kind, ok: true, data: httpPostJson(globalNode.ssoBase + "/application/o/device/", body, 25) }

    else if kind = "devicePoll"
        body = "client_id=" + urlEncode(globalNode.tvClientId)
        body = body + "&grant_type=" + urlEncode("urn:ietf:params:oauth:grant-type:device_code")
        body = body + "&device_code=" + urlEncode(req.deviceCode)
        result = httpPostJson(globalNode.ssoBase + "/application/o/token/", body, 25)
        ' A pending authorisation is the normal case while somebody is still
        ' typing on their phone, so it is reported as data rather than failure.
        m.top.response = { kind: kind, ok: true, data: result }

    else if kind = "pages"
        ' An archive.org comic or photo set. The server turns the item into a
        ' list of plain JPEGs, because a Roku can draw those and can draw
        ' neither a PDF nor a details page.
        url = globalNode.searchBase + "/api/pages?id=" + urlEncode(req.id)
        if req.pageKind <> invalid and req.pageKind <> ""
            url = url + "&kind=" + urlEncode(req.pageKind)
        end if
        result = httpGetJson(url, 30)
        ok = result <> invalid and result.pages <> invalid
        m.top.response = { kind: kind, ok: ok, data: result }

    else if kind = "health"
        ' The address under test is carried in the request, not read off the
        ' global node: the whole point is to try a server BEFORE committing to
        ' it, so the one currently configured is the wrong thing to ask.
        result = httpProbe(req.base + "/api/health", 15)
        m.top.response = { kind: kind, ok: (result.code = 200), data: result }

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
' Collect the rest of a search that has already answered.
'
' Returns the settled result, or invalid when there was nothing more to get --
' a cache hit, a search with no job, or a collection that never grew. Returning
' invalid rather than the first paint again is what keeps this to one extra
' repaint instead of a guaranteed second one.
function collectSearch(globalNode as Object, first as Object, qs as String) as Object
    ' Split rather than chained with `or`: these are equality tests against
    ' invalid, and a chained comparison that evaluates both sides is a type
    ' mismatch at runtime rather than a false.
    if first = invalid then return invalid
    if first.job = invalid then return invalid
    if first.job = "" then return invalid
    if first.complete = true then return invalid

    latest = invalid
    ' Twelve tries at a second apart covers the fast indexer tier comfortably
    ' and most of the slow one. A television is not the place to keep a network
    ' task alive for a minute chasing the last few results.
    for i = 1 to 12
        sleep(1000)
        upd = httpGetJson(globalNode.searchBase + "/api/search/updates?" + qs + "&job=" + urlEncode(first.job), 20)
        if upd = invalid then exit for
        ' The job was swept. What is already on screen is still a real answer.
        if upd.gone = true then exit for
        latest = upd
        if upd.complete = true then exit for
    end for

    if latest = invalid then return invalid
    if latest.cards = invalid then return invalid
    if first.cards <> invalid and latest.cards.count() <= first.cards.count() then return invalid
    return latest
end function

function httpGetJson(url as String, timeoutSec as Integer) as Object
    xfer = CreateObject("roUrlTransfer")
    xfer.setUrl(url)
    xfer.setCertificatesFile("common:/certs/ca-bundle.crt")
    xfer.initClientCertificates()
    xfer.addHeader("Accept", "application/json")

    ' Prove who we are. The device flow leaves an access token in the registry
    ' and it is the only credential this box has -- there is no cookie jar on a
    ' television. Without sending it the server answers 401, which BrightScript
    ' cannot tell apart from a dead network, so a signed-in TV reported
    ' "could not reach the search service" instead of "you are not signed in".
    reg = CreateObject("roRegistrySection", "yarrit")
    if reg.exists("access_token")
        token = reg.read("access_token")
        if token <> invalid and token <> ""
            xfer.addHeader("Authorization", "Bearer " + token)
        end if
    end if

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

    ' Same guard as the POST path: an empty body must not reach ParseJson,
    ' which logs an error for every occurrence rather than returning quietly.
    text = msg.getString()
    if text = invalid or text = "" then return invalid

    return ParseJson(text)
end function

' httpProbe reports how a request ended rather than what it returned.
'
' httpGetJson collapses a typo'd hostname, a 404, a 500, a TLS refusal and a
' dead network into one indistinguishable invalid. That is fine when the caller
' only wants the body, but it is exactly the wrong answer for the settings
' screen, whose entire job is telling the owner WHICH of those went wrong.
'
' A response code of 0 means the transfer never started; anything else negative
' is Roku's own transport failure code rather than an HTTP status.
function httpProbe(url as String, timeoutSec as Integer) as Object
    xfer = CreateObject("roUrlTransfer")
    xfer.setUrl(url)
    xfer.setCertificatesFile("common:/certs/ca-bundle.crt")
    xfer.initClientCertificates()
    xfer.addHeader("Accept", "application/json")

    port = CreateObject("roMessagePort")
    xfer.setMessagePort(port)

    if not xfer.asyncGetToString() then return { code: 0, text: "", reason: "That address is not a URL this TV can open." }

    msg = wait(timeoutSec * 1000, port)
    if type(msg) <> "roUrlEvent"
        xfer.asyncCancel()
        return { code: 0, text: "", reason: "No answer within " + Str(timeoutSec).trim() + " seconds." }
    end if

    text = msg.getString()
    if text = invalid then text = ""
    reason = msg.getFailureReason()
    if reason = invalid then reason = ""

    return { code: msg.getResponseCode(), text: text, reason: reason }
end function

' httpPostJson posts a form body and parses the JSON reply.
'
' Errors are parsed too rather than discarded: the device flow signals
' "still waiting" with a 400 and an error code in the body, so throwing away
' non-200 responses would make a normal pending poll indistinguishable from a
' dead network.
function httpPostJson(url as String, body as String, timeoutSec as Integer) as Object
    xfer = CreateObject("roUrlTransfer")
    xfer.setUrl(url)
    xfer.setCertificatesFile("common:/certs/ca-bundle.crt")
    xfer.initClientCertificates()
    xfer.addHeader("Content-Type", "application/x-www-form-urlencoded")
    xfer.addHeader("Accept", "application/json")

    port = CreateObject("roMessagePort")
    xfer.setMessagePort(port)

    if not xfer.asyncPostFromString(body) then return invalid

    msg = wait(timeoutSec * 1000, port)
    if type(msg) <> "roUrlEvent"
        xfer.asyncCancel()
        return invalid
    end if

    ' An empty body is a normal outcome here -- the device poll gets one
    ' whenever the request is cancelled or the peer closes early -- and handing
    ' it to ParseJson logs "Data is empty" on every single poll. That buries
    ' any genuine error in a scrolling wall of noise, which is the state the
    ' log was in when this was found.
    text = msg.getString()
    if text = invalid or text = "" then return invalid

    return ParseJson(text)
end function

' urlEncode escapes a value for use in a query string. roUrlTransfer.escape()
' handles this, but it needs an instance, so one is kept around.
function urlEncode(value as String) as String
    if m.escaper = invalid then m.escaper = CreateObject("roUrlTransfer")
    return m.escaper.escape(value)
end function

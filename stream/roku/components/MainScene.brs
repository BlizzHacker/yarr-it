' Stream — Roku scene.
'
' Flow: browse or search -> pick a title -> pick a source -> the LAN gateway
' turns that magnet into an HTTP stream -> the Video node plays it.
'
' The channel never touches BitTorrent itself; Roku cannot. Everything about
' swarms happens on the gateway.

sub init()
    m.grid        = m.top.findNode("grid")
    m.status      = m.top.findNode("status")
    m.busy        = m.top.findNode("busy")
    m.busyText    = m.top.findNode("busyText")
    m.player      = m.top.findNode("player")
    m.sourcePane  = m.top.findNode("sourcePane")
    m.pageViewer  = m.top.findNode("pageViewer")
    m.pageImage   = m.top.findNode("pageImage")
    m.pageCount   = m.top.findNode("pageCount")
    m.sourceList  = m.top.findNode("sourceList")
    m.sourceTitle = m.top.findNode("sourceTitle")
    m.sourceHint  = m.top.findNode("sourceHint")
    m.api           = m.top.findNode("api")
    m.filterButtons = m.top.findNode("filterButtons")
    m.filterEcho    = m.top.findNode("filterEcho")
    m.signIn        = m.top.findNode("signIn")
    m.signInStep1   = m.top.findNode("signInStep1")
    m.signInCode    = m.top.findNode("signInCode")
    m.signInHint    = m.top.findNode("signInHint")
    m.signInStatus  = m.top.findNode("signInStatus")
    m.signInUrl     = m.top.findNode("signInUrl")
    m.pollTimer     = m.top.findNode("pollTimer")

    m.api.observeField("response", "onApiResponse")
    m.grid.observeField("itemSelected", "onTitleSelected")
    m.sourceList.observeField("itemSelected", "onSourceSelected")
    m.player.observeField("state", "onPlayerState")

    m.cards = []          ' current result set
    m.activeCard = invalid
    m.query = ""          ' last search term; "" means browse
    m.filterIndex = 0
    m.sort = "seeders"

    ' The filter set. `kind` narrows to what a thing is; `groups` narrows by
    ' catalogue section -- Movies needs the group because a film and a TV
    ' episode are both kind=video.
    m.filters = [
        { label: "All",    kind: "",      groups: "" },
        { label: "Movies", kind: "",      groups: "movies" },
        { label: "TV",     kind: "",      groups: "tv" },
        { label: "Comics", kind: "comic", groups: "" },
        { label: "Images", kind: "image", groups: "" },
    ]

    labels = []
    for each f in m.filters
        labels.push(f.label)
    end for
    m.filterButtons.buttons = labels
    m.filterButtons.observeField("buttonSelected", "onFilterSelected")
    updateFilterEcho()

    m.pollTimer.observeField("fire", "onPollTick")

    ' Nothing loads until this TV belongs to a real account. The check is here
    ' rather than after the first screen so no content is ever fetched, let
    ' alone shown, by an unauthenticated device.
    if hasValidToken()
        startBrowsing()
    else
        beginSignIn()
    end if
end sub

' ------------------------------------------------------------------ auth ----
'
' RFC 8628 device flow against Authentik. The TV shows a short code; the person
' approves it on a phone. Only Authentik accounts can complete it, so the
' channel is closed to anyone without one -- which is the point.


function tokenStore() as Object
    return CreateObject("roRegistrySection", "yarrit")
end function

function hasValidToken() as Boolean
    reg = tokenStore()
    if not reg.exists("access_token") then return false
    if not reg.exists("expires_at") then return false
    ' Treated as expired a minute early so a token cannot die mid-request.
    return Val(reg.read("expires_at")) > (nowSeconds() + 60)
end function

function nowSeconds() as Integer
    d = CreateObject("roDateTime")
    return d.asSeconds()
end function

sub beginSignIn()
    m.signIn.visible = true
    m.grid.setFocus(false)
    m.signInStep1.text = "On your phone or computer, go to:"
    m.signInCode.text = "…"
    m.signInHint.text = ""
    m.signInStatus.text = "Requesting a code…"
    dispatch({ kind: "deviceStart" })
end sub

sub onDeviceStart(data as Object)
    if data = invalid or data.user_code = invalid
        m.signInStatus.text = "Could not reach the sign-in service. It will retry shortly."
        m.pollTimer.duration = 15
        m.pollTimer.control = "start"
        return
    end if

    m.deviceCode = data.device_code
    ' The provider's own hostname is long and easy to mistype from across a
    ' room. yarrit.com/tv redirects to exactly the same page and is
    ' short enough to read off a screen and get right first time.
    m.signInStep1.text = "On your phone or computer, go to:"
    m.signInCode.text = data.user_code
    m.signInUrl.text = "yarrit.com/tv"
    m.signInHint.text = "Then enter this code. It expires in " + Str(Int(data.expires_in / 60)).trim() + " minutes."
    m.signInStatus.text = "Waiting for you to approve this TV…"

    ' Roku's screensaver takes over an idle sign-in screen and the flow stalls
    ' behind it: the code quietly expires and nothing asks for another, so
    ' somebody who walked away returns to a dead number. Remembering the
    ' deadline lets the screen heal itself rather than depend on a timer that
    ' may not have been running.
    ttl = 600
    if data.expires_in <> invalid then ttl = data.expires_in
    m.codeExpiresAt = nowSeconds() + ttl

    ' The server states its own poll interval; honouring it is what keeps a
    ' fleet of TVs from hammering the identity provider.
    interval = 5
    if data.interval <> invalid and data.interval > 0 then interval = data.interval
    m.pollTimer.duration = interval
    m.pollTimer.control = "start"
end sub

sub onPollTick()
    if m.deviceCode = invalid
        dispatch({ kind: "deviceStart" })
        return
    end if

    ' Self-heal: a code that has aged out is replaced without waiting for the
    ' server to tell us, which it only does when a poll actually gets through.
    if m.codeExpiresAt <> invalid and nowSeconds() > m.codeExpiresAt
        m.signInStatus.text = "That code expired. Getting a new one…"
        m.deviceCode = invalid
        m.codeExpiresAt = invalid
        dispatch({ kind: "deviceStart" })
        return
    end if
    dispatch({ kind: "devicePoll", deviceCode: m.deviceCode })
end sub

sub onDevicePoll(data as Object)
    if data = invalid then return

    if data.access_token <> invalid
        reg = tokenStore()
        reg.write("access_token", data.access_token)
        ttl = 3600
        if data.expires_in <> invalid then ttl = data.expires_in
        reg.write("expires_at", Str(nowSeconds() + ttl).trim())
        if data.refresh_token <> invalid then reg.write("refresh_token", data.refresh_token)
        reg.flush()

        m.pollTimer.control = "stop"
        m.signIn.visible = false
        startBrowsing()
        return
    end if

    err = ""
    if data.error <> invalid then err = data.error

    if err = "authorization_pending" or err = "slow_down"
        ' Normal: the person has not finished on their phone yet.
        if err = "slow_down" then m.pollTimer.duration = m.pollTimer.duration + 5
        return
    end if

    if err = "expired_token" or err = "access_denied"
        m.signInStatus.text = "That code expired. Getting a new one…"
        m.deviceCode = invalid
        dispatch({ kind: "deviceStart" })
        return
    end if

    if err <> "" then m.signInStatus.text = "Sign-in failed: " + err
end sub

sub startBrowsing()
    m.signIn.visible = false
    m.grid.setFocus(true)
    showBusy("Loading…")
    dispatch({ kind: "discover" })
end sub

' ---------------------------------------------------------------- filters ----

sub onFilterSelected()
    idx = m.filterButtons.buttonSelected
    if idx < 0 or idx >= m.filters.count() then return

    m.filterIndex = idx
    updateFilterEcho()

    ' The source list belongs to a title from the previous result set. Leaving
    ' it up means the new results load behind a pane describing something that
    ' is no longer on screen.
    m.sourcePane.visible = false
    m.activeCard = invalid

    ' Focus goes back to the results, because choosing a filter is a request to
    ' look at them -- leaving focus on the bar means every selection needs an
    ' extra press to get anywhere.
    m.grid.setFocus(true)
    runCurrentQuery()
end sub

' runCurrentQuery re-runs whatever is on screen under the current filter.
'
' The same call covers both browsing a category and narrowing an existing
' search, which is why the query is kept in m.query rather than read back off
' a widget: the two must not drift apart.
sub runCurrentQuery()
    f = m.filters[m.filterIndex]

    if m.query = "" and f.kind = "" and f.groups = ""
        ' "All" with nothing typed is the home screen, not a search for
        ' everything -- the server would rightly refuse that.
        showBusy("Loading…")
        dispatch({ kind: "discover" })
        return
    end if

    what = f.label
    if m.query <> "" then what = Chr(34) + m.query + Chr(34) + " in " + f.label
    showBusy("Finding the best " + what + "…" + Chr(10) + Chr(10) + "Ranking by health, so the first row is the one most likely to play.")
    dispatch({
        kind: "search",
        query: m.query,
        filterKind: f.kind,
        filterGroups: f.groups,
        sort: m.sort,
    })
end sub

sub updateFilterEcho()
    f = m.filters[m.filterIndex]
    order = "best seeded first"
    if m.sort = "relevance" then order = "best match first"
    m.filterEcho.text = "Showing: " + f.label + "   -   " + order
end sub

' dispatch sends a request to the network task.
'
' Setting .request alone does nothing: a SceneGraph Task only executes when its
' control field is set to RUN, and it must be re-set for every subsequent run.
' Forgetting this makes the UI sit on a spinner forever with no error anywhere,
' because the task simply never ran.
sub dispatch(req as Object)
    m.api.control = "STOP"
    m.api.request = req
    m.api.control = "RUN"
end sub

' ---------------------------------------------------------------- remote ----

function onKeyEvent(key as String, press as Boolean) as Boolean
    if not press then return false

    ' The page viewer takes the remote while it is open. Checked first so a
    ' page turn is never mistaken for navigation in the grid behind it.
    if m.pageViewer <> invalid and m.pageViewer.visible
        if key = "right" or key = "down"
            showPage(m.pageIndex + 1)
            return true
        else if key = "left" or key = "up"
            showPage(m.pageIndex - 1)
            return true
        else if key = "back"
            closePages()
            return true
        end if
        return true
    end if

    ' While the gate is up every key is consumed except Back, so no amount of
    ' button-mashing reaches the catalogue behind it.
    if m.signIn.visible
        ' Waking the TV is exactly when a returning viewer looks at the code,
        ' so a stale one is refreshed on the first press rather than after the
        ' next poll interval.
        if m.codeExpiresAt <> invalid and nowSeconds() > m.codeExpiresAt
            m.deviceCode = invalid
            m.codeExpiresAt = invalid
            m.signInStatus.text = "Refreshing your code…"
            dispatch({ kind: "deviceStart" })
        end if
        return key <> "back"
    end if

    ' UP from the grid reaches the filter bar; DOWN comes back. Without an
    ' explicit hop the bar is unreachable, because a MarkupGrid consumes UP to
    ' move between its own rows and never yields focus upward.
    ' Keyed on what is NOT showing rather than on the grid holding focus:
    ' while results are still loading the grid has not taken focus yet, so a
    ' hasFocus() test silently dropped the key and UP appeared to do nothing.
    if key = "up" and not m.sourcePane.visible and not m.player.visible and not m.filterButtons.hasFocus()
        ' Only from the top row, so UP still moves between rows everywhere else.
        if m.grid.itemFocused < m.grid.numColumns
            m.filterButtons.setFocus(true)
            return true
        end if
        return false
    end if
    if key = "down" and m.filterButtons.hasFocus()
        m.grid.setFocus(true)
        return true
    end if

    ' Back unwinds one layer at a time rather than exiting outright.
    if key = "back"
        if m.player.visible
            stopPlayback()
            return true
        else if m.filterButtons.hasFocus()
            m.grid.setFocus(true)
            return true
        else if m.sourcePane.visible
            m.sourcePane.visible = false
            m.grid.setFocus(true)
            return true
        end if
        return false
    end if

    ' The star / options button opens search from anywhere.
    if key = "options"
        showKeyboard()
        return true
    end if

    return false
end function

' ---------------------------------------------------------------- search ----

sub showKeyboard()
    dlg = CreateObject("roSGNode", "KeyboardDialog")
    dlg.title = "Search"
    dlg.buttons = ["Search", "Cancel"]
    m.keyboard = dlg
    dlg.observeField("buttonSelected", "onKeyboardButton")
    m.top.dialog = dlg
end sub

sub onKeyboardButton()
    dlg = m.keyboard
    if dlg = invalid then return

    if dlg.buttonSelected = 0
        query = dlg.text
        m.top.dialog.close = true
        if query <> invalid and query.trim() <> ""
            ' Kept on the scene so the filter chips can re-run the same search
            ' without reopening the keyboard.
            m.query = query.trim()
            runCurrentQuery()
        end if
    else
        m.top.dialog.close = true
    end if
end sub

' ------------------------------------------------------------------- api ----

sub onApiResponse()
    resp = m.api.response
    if resp = invalid then return

    if resp.kind = "deviceStart"
        onDeviceStart(resp.data)
        return
    else if resp.kind = "devicePoll"
        onDevicePoll(resp.data)
        return
    end if

    if resp.kind = "discover"
        hideBusy()
        if resp.data = invalid or resp.data.rows = invalid
            setStatus("Could not reach the search service. Press the * button to search anyway.")
            return
        end if
        ' Flatten the discover rails into one grid: rows-of-rows is fiddly with
        ' a D-pad, and the point here is "something to look at on launch".
        items = []
        for each row in resp.data.rows
            for each item in row.items
                items.push({
                    title: item.title
                    poster: item.poster
                    year: item.year
                    seeders: -1          ' unknown until searched
                    isDiscover: true
                })
            end for
        end for
        showCards(items)
        setStatus("")

    else if resp.kind = "search"
        hideBusy()
        if resp.data = invalid or resp.data.cards = invalid or resp.data.cards.count() = 0
            ' Naming the filter matters: an empty Comics result reads as a
            ' broken app unless it says which filter produced it.
            setStatus("Nothing found in " + m.filters[m.filterIndex].label + ". Press * to search, or UP to change the filter.")
            return
        end if
        m.cards = resp.data.cards
        items = []
        for each c in resp.data.cards
            poster = ""
            if c.art <> invalid and c.art.poster <> invalid then poster = c.art.poster
            instant = false
            if c.instant <> invalid then instant = c.instant
            items.push({
                title: c.title
                poster: poster
                year: c.year
                seeders: c.seeders
                instant: instant
                isDiscover: false
            })
        end for
        showCards(items)
        ' Say what is being shown and how it is ordered. On a TV there is no
        ' URL bar and no second window, so the screen is the only thing that
        ' can explain why these results and not others.
        setStatus(Str(resp.data.cards.count()).trim() + " results in " + m.filters[m.filterIndex].label + ", best first")

    else if resp.kind = "pages"
        onPagesLoaded(resp.data)

    else if resp.kind = "prepare"
        if not resp.ok
            hideBusy()
            reason = "The gateway could not start this torrent."
            if resp.data <> invalid and resp.data.error <> invalid then reason = resp.data.error
            setStatus(reason + "  Try a different source.")
            m.sourcePane.visible = true
            m.sourceList.setFocus(true)
            return
        end if
        startPlayback(resp.data)
    end if
end sub

' ----------------------------------------------------------------- browse ---

sub showCards(items as Object)
    content = CreateObject("roSGNode", "ContentNode")
    for each it in items
        node = content.createChild("ContentNode")
        node.title = it.title
        if it.poster <> invalid and it.poster <> "" then node.HDGRIDPOSTERURL = it.poster
        ' An archive.org item is served by a host that is always up, so it has
        ' no seeders and the question does not apply. Printing "0 up" on it
        ' reads as a dead torrent -- the opposite of the truth, since those are
        ' the results most certain to play.
        ' Guarded: the discover path builds items without this field, and
        ' comparing invalid to a boolean is a runtime error, not false.
        if it.instant <> invalid and it.instant = true
            node.addFields({ seedText: "instant" })
        else if it.seeders > 0
            node.addFields({ seedText: Str(it.seeders).trim() + " up" })
        else
            node.addFields({ seedText: "" })
        end if
    end for
    m.grid.content = content
    m.grid.jumpToItem = 0
    m.grid.setFocus(true)
end sub

sub onTitleSelected()
    idx = m.grid.itemSelected

    ' A discover tile has no sources yet — selecting it runs a real search.
    if m.cards.count() = 0 or idx >= m.cards.count()
        node = m.grid.content.getChild(idx)
        if node = invalid then return
        showBusy("Finding sources for " + Chr(34) + node.title + Chr(34) + "…")
        dispatch({ kind: "search", query: node.title })
        return
    end if

    card = m.cards[idx]
    m.activeCard = card
    showSources(card)
end sub

' ---------------------------------------------------------------- sources ---

sub showSources(card as Object)
    m.sourceTitle.text = card.title
    m.sourceHint.text = Str(card.sources.count()).trim() + " sources — MP4 is most likely to play on a TV"

    rows = []
    for each s in card.sources
        quality = s.quality
        if quality = invalid or quality = "" then quality = "—"
        codec = s.codec
        if codec = invalid or codec = "" then codec = "?"
        ' Container matters more than codec on Roku, so it leads the row.
        container = containerOf(s.title)
        rows.push(container + "   " + quality + "   " + codec + "   " + s.sizeHuman + "   " + Str(s.seeders).trim() + " up   " + s.indexer)
    end for

    m.sourceList.content = buildLabelContent(rows)
    m.sourcePane.visible = true
    m.sourceList.jumpToItem = 0
    m.sourceList.setFocus(true)
end sub

sub onSourceSelected()
    idx = m.sourceList.itemSelected
    if m.activeCard = invalid or idx >= m.activeCard.sources.count() then return

    src = m.activeCard.sources[idx]
    m.sourcePane.visible = false

    ' A comic or a photo set is not a stream. It has no magnet, and its URL is
    ' an archive.org details page -- HTML. Sending that to the Video node is
    ' what produced "this file would not play". Ask the server to turn the item
    ' into pictures instead.
    kind = ""
    if m.activeCard.kind <> invalid then kind = m.activeCard.kind
    if kind = "comic" or kind = "image"
        itemId = src.magnet
        if itemId = invalid or itemId = "" then itemId = src.url
        if itemId = invalid or itemId = "" then itemId = m.activeCard.key
        print "YARRIT: opening "; kind; " item "; itemId
        showBusy("Fetching pages…")
        dispatch({ kind: "pages", id: itemId, pageKind: kind })
        return
    end if

    showBusy("Joining the swarm…" + Chr(10) + Chr(10) + "The gateway is fetching metadata and the first pieces. This can take up to a minute on a quiet torrent.")
    dispatch({ kind: "prepare", magnet: src.magnet })
end sub

' ------------------------------------------------------------ page viewer ---

sub onPagesLoaded(data as Object)
    hideBusy()
    if data = invalid or data.pages = invalid or data.pages.count() = 0
        setStatus("Nothing readable in this item.")
        return
    end if

    m.pages = data.pages
    m.pageIndex = 0
    ' A comic's page count is not published, so the list is a ceiling and the
    ' viewer stops at the first page that will not load.
    m.pagesProbe = (data.probe = true)
    print "YARRIT: viewer open with "; m.pages.count(); " pages"
    m.pageViewer.visible = true
    m.pageViewer.setFocus(true)
    showPage(0)
end sub

sub showPage(i as Integer)
    if m.pages = invalid or i < 0 or i >= m.pages.count() then return
    m.pageIndex = i
    print "YARRIT: page "; i; " -> "; m.pages[i]
    m.pageImage.uri = m.pages[i]
    if m.pagesProbe
        m.pageCount.text = "Page " + Str(i + 1).trim() + "   ←  →  to turn,  Back to close"
    else
        m.pageCount.text = Str(i + 1).trim() + " / " + Str(m.pages.count()).trim() + "   ←  →  to turn,  Back to close"
    end if
end sub

sub closePages()
    m.pageViewer.visible = false
    m.pageImage.uri = ""
    m.pages = invalid
    m.grid.setFocus(true)
end sub

function containerOf(name as String) as String
    n = LCase(name)
    if Instr(1, n, ".mp4") > 0 or Instr(1, n, "x264") > 0 then return "MP4 "
    if Instr(1, n, ".mkv") > 0 then return "MKV "
    if Instr(1, n, ".avi") > 0 then return "AVI!"
    return "    "
end function

function buildLabelContent(rows as Object) as Object
    content = CreateObject("roSGNode", "ContentNode")
    for each r in rows
        n = content.createChild("ContentNode")
        n.title = r
    end for
    return content
end function

' ---------------------------------------------------------------- player ----

sub startPlayback(info as Object)
    hideBusy()

    video = CreateObject("roSGNode", "ContentNode")
    video.url = info.streamUrl
    video.title = info.name
    ' The gateway serves the raw container; let Roku sniff rather than lying
    ' about the type, which produces clearer errors when it cannot play it.
    video.streamformat = streamFormatFor(info.file)

    m.player.content = video
    m.player.visible = true
    m.player.setFocus(true)
    m.player.control = "play"

    if info.playable = false and info.reason <> invalid
        setStatus(info.reason)
    end if
end sub

function streamFormatFor(name as String) as String
    n = LCase(name)
    if Instr(1, n, ".mp4") > 0 or Instr(1, n, ".m4v") > 0 then return "mp4"
    if Instr(1, n, ".mkv") > 0 then return "mkv"
    if Instr(1, n, ".webm") > 0 then return "mkv"   ' Roku treats webm as matroska
    return "mp4"
end function

sub onPlayerState()
    state = m.player.state
    if state = "error"
        stopPlayback()
        msg = "This file would not play on Roku."
        err = m.player.errorMsg
        if err <> invalid and err <> "" then msg = msg + "  (" + err + ")"
        setStatus(msg + "  Try an MP4/H.264 source — Roku cannot play every container.")
    else if state = "finished"
        stopPlayback()
    end if
end sub

sub stopPlayback()
    m.player.control = "stop"
    m.player.visible = false
    m.player.content = invalid
    m.grid.setFocus(true)
end sub

' ------------------------------------------------------------------- ui -----

sub showBusy(text as String)
    m.busyText.text = text
    m.busy.visible = true
end sub

sub hideBusy()
    m.busy.visible = false
end sub

sub setStatus(text as String)
    m.status.text = text
    ' Nudge the grid down when a message is showing so they do not overlap.
    if text = ""
        m.grid.translation = [80, 170]
    else
        m.grid.translation = [80, 230]
    end if
end sub

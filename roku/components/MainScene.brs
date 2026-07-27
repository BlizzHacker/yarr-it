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
    m.sourceList  = m.top.findNode("sourceList")
    m.sourceTitle = m.top.findNode("sourceTitle")
    m.sourceHint  = m.top.findNode("sourceHint")
    m.api         = m.top.findNode("api")

    m.api.observeField("response", "onApiResponse")
    m.grid.observeField("itemSelected", "onTitleSelected")
    m.sourceList.observeField("itemSelected", "onSourceSelected")
    m.player.observeField("state", "onPlayerState")

    m.cards = []          ' current result set
    m.activeCard = invalid

    m.grid.setFocus(true)
    showBusy("Loading…")
    dispatch({ kind: "discover" })
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

    ' Back unwinds one layer at a time rather than exiting outright.
    if key = "back"
        if m.player.visible
            stopPlayback()
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
            showBusy("Searching every index for " + Chr(34) + query + Chr(34) + "…" + Chr(10) + Chr(10) + "A cold search can take a few seconds.")
            dispatch({ kind: "search", query: query })
        end if
    else
        m.top.dialog.close = true
    end if
end sub

' ------------------------------------------------------------------- api ----

sub onApiResponse()
    resp = m.api.response
    if resp = invalid then return

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
            setStatus("Nothing found. Press the * button to try another search.")
            return
        end if
        m.cards = resp.data.cards
        items = []
        for each c in resp.data.cards
            poster = ""
            if c.art <> invalid and c.art.poster <> invalid then poster = c.art.poster
            items.push({
                title: c.title
                poster: poster
                year: c.year
                seeders: c.seeders
                isDiscover: false
            })
        end for
        showCards(items)
        setStatus("")

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
        if it.seeders >= 0
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
    showBusy("Joining the swarm…" + Chr(10) + Chr(10) + "The gateway is fetching metadata and the first pieces. This can take up to a minute on a quiet torrent.")
    dispatch({ kind: "prepare", magnet: src.magnet })
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

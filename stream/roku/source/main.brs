' Channel entry point.
'
' Roku runs this on the main thread, then everything else happens inside the
' SceneGraph scene. The only job here is to create the screen and pump the
' message port until the user exits.

sub Main()
    screen = CreateObject("roSGScreen")
    port = CreateObject("roMessagePort")
    screen.setMessagePort(port)

    scene = screen.CreateScene("MainScene")
    screen.show()

    ' Global node: anything the scene and its tasks both need to read.
    global = screen.getGlobalNode()
    global.addFields({
        searchBase: "https://stream.moveweight.com"
        gatewayBase: "http://192.168.0.118:8900"
    })

    while true
        msg = wait(0, port)
        if type(msg) = "roSGScreenEvent"
            if msg.isScreenClosed() then return
        end if
    end while
end sub

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

    ' Shared config the scene and its tasks both read. Note the name: 'global'
    ' is a reserved BrightScript identifier bound to ifGlobal, and assigning a
    ' node to it is a runtime type mismatch.
    globalNode = screen.getGlobalNode()
    globalNode.addFields({
        searchBase: "https://yarrit.com"
        gatewayBase: "http://192.168.0.118:8900"
        ssoBase: "https://auth.yarrit.com"
        ' Public client: a TV cannot hold a secret, so the device-code flow is
        ' used and there is nothing here worth extracting from the package.
        tvClientId: "OCXCp0GZKbRpcppKPvKwUhHp2B8lDVHZLpJ5Mznt"
    })

    while true
        msg = wait(0, port)
        if type(msg) = "roSGScreenEvent"
            if msg.isScreenClosed() then return
        end if
    end while
end sub

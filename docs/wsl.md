# Using Builder from WSL

MobAI runs on Windows, and builder runs inside WSL. By default WSL has its own
network, so `localhost` in WSL is not Windows. There are two ways to connect
them.

## Mirrored networking (recommended)

On Windows 11 22H2 or later, WSL can share the Windows network. Linux then
reaches Windows servers on `localhost`, so builder talks to MobAI on the default
`http://localhost:8686` with no extra MobAI settings. See Microsoft's
[mirrored mode networking](https://learn.microsoft.com/en-us/windows/wsl/networking#mirrored-mode-networking)
guide for details.

1. Create or edit `%UserProfile%\.wslconfig` on Windows:

   ```ini
   [wsl2]
   networkingMode=mirrored
   ```

2. Restart WSL from PowerShell, then open your distribution again:

   ```powershell
   wsl --shutdown
   ```

3. Use the default MobAI URL. If `builder.json` sets `mobai.url` to your PC
   name, remove it or set it back:

   ```json
   {
     "mobai": {
       "url": "http://localhost:8686"
     }
   }
   ```

4. Check the connection:

   ```bash
   builder mobai ping
   ```

   It prints `success`.

5. Run `builder dev flutter` once. It rewrites Flutter's `mobai-ios` custom
   device with the new URL, so `flutter attach` uses it too.

### React Native

The app on the phone loads JavaScript from Metro, which runs inside WSL. With
mirrored networking the Hyper-V firewall blocks inbound connections to WSL by
default. If the app can't reach Metro, allow its port from PowerShell as
administrator:

```powershell
New-NetFirewallHyperVRule -Name "Metro" -DisplayName "Metro bundler" -Direction Inbound -VMCreatorId '{40E0AC32-46A5-438A-A0B2-2B479E8F2E90}' -Protocol TCP -LocalPorts 8081
```

Change `8081` if you run Metro on another port (`--metro-port`).

## Default networking (NAT)

Without mirrored networking, builder reaches MobAI over the network:

1. In MobAI, go to **Integrations → API server** and enable **Allow external
   connections**.
2. Get your Windows hostname and use it with a `.local` suffix:

   ```bash
   hostname.exe
   ```

   ```json
   {
     "mobai": {
       "url": "http://YOUR-PC-NAME.local:8686"
     }
   }
   ```

3. If MobAI has an API token set, put it in `.env` in the directory you run
   builder from (or export it):

   ```bash
   MOBAI_ACCESS_KEY=your-mobai-api-token
   ```

   Without it every call fails with `invalid or missing API token`.

## Device in use

Builder claims the device before installing or launching an app. If you started
the device's bridge in the MobAI app and builder reports the device is in use,
stop the bridge in the MobAI app and run builder again.

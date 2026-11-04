package controller

const ServiceName = "yoloctl-controller.service"

const ServiceUnit = `[Unit]
Description=yoloctl space controller
After=local-fs.target containerd.service
Wants=containerd.service

[Service]
Type=simple
ExecStart=/opt/yoloctl/bin/yoloctl controller-internal serve
User=root
Group=root
UMask=0077
KillMode=control-group
TimeoutStopSec=10
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`

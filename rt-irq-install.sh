#!/bin/bash

# Update package repositories
sudo apt update

# Install rtirq
sudo apt install -y rtirq-init

# Enable rtirq service
sudo systemctl enable rtirq

# Start rtirq service
sudo systemctl start rtirq

# Verify service status
sudo systemctl status rtirq --no-pager

echo "rtirq installation and setup complete."
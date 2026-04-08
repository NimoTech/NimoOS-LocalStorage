#!/bin/bash

set -e

readonly APP_NAME="nimoos-local-storage"
readonly APP_NAME_FIRST="nimoos-local-storage-first"
readonly APP_NAME_SHORT="local-storage"

# copy config files
readonly CONF_PATH=/etc/nimoos
readonly CONF_FILE=${CONF_PATH}/${APP_NAME_SHORT}.conf
readonly CONF_FILE_SAMPLE=${CONF_PATH}/${APP_NAME_SHORT}.conf.sample

if [ ! -f "${CONF_FILE}" ]; then \
    echo "Initializing config file..."
    cp -v "${CONF_FILE_SAMPLE}" "${CONF_FILE}"; \
fi

systemctl daemon-reload

# enable service (without starting)
echo "Enabling service..."
systemctl enable --force --no-ask-password "${APP_NAME}.service"
systemctl enable --force --no-ask-password "${APP_NAME_FIRST}.service"

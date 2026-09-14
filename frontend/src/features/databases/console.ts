/**
 * Where phpMyAdmin lives.
 *
 * A constant, and panel-relative on purpose. phpMyAdmin is served by the
 * panel's own nginx on the loopback and is reached only through the panel's
 * own `/phpmyadmin/` location, which addresses it by a fixed internal name. So
 * its address is always this panel's address plus this path — there is nothing
 * to configure, and there used to be a field asking an operator to choose a
 * host name that no request could ever arrive with.
 *
 * It matches `protocol.ConsoleMount` in the Go tree, the location block in
 * `docker/nginx/dev.conf`, and the one in `scripts/jothost-installer.sh`.
 */
export const CONSOLE_MOUNT = '/phpmyadmin/';

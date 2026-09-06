package pma

import (
	"strings"
	"text/template"
)

// configTemplate is the configuration the panel writes for phpMyAdmin.
//
// What is deliberately absent matters more than what is present: there are no
// credentials here. auth_type is cookie, so a visitor signs in with a database
// account the panel created and phpMyAdmin holds nothing that would let an
// unauthenticated visitor do anything. A config with a stored user and password
// — which several hosting guides suggest — turns reaching the page into having
// the database.
var configTemplate = template.Must(template.New("phpmyadmin").Parse(
	`<?php
/**
 * phpMyAdmin configuration written by JotHost Panel.
 *
 * Edits to this file are replaced the next time the panel installs or
 * reconfigures phpMyAdmin.
 */

declare(strict_types=1);

/*
 * Encrypts the cookie that carries a signed-in user's database password.
 * Anyone holding it can forge a session, so this file is mode 0640 and owned
 * by the account the FPM pool runs as.
 */
$cfg['blowfish_secret'] = '{{ .Secret }}';

$i = 0;

$i++;
$cfg['Servers'][$i]['auth_type'] = 'cookie';
$cfg['Servers'][$i]['host'] = 'localhost';
{{- if .Socket }}
$cfg['Servers'][$i]['connect_type'] = 'socket';
$cfg['Servers'][$i]['socket'] = '{{ .Socket }}';
{{- end }}
$cfg['Servers'][$i]['compress'] = false;

/*
 * An account with no password is never accepted, whatever the server would
 * allow. A blank-password root account is the single most common way a
 * phpMyAdmin installation becomes an open door.
 */
$cfg['Servers'][$i]['AllowNoPassword'] = false;

/*
 * root cannot sign in here at all. Administering the server is the panel's
 * job; this page exists so a customer can work with their own database.
 */
$cfg['Servers'][$i]['AllowRoot'] = false;

/* Scratch space, outside the webroot so nothing written can be served. */
$cfg['TempDir'] = '{{ .TempDir }}';

/* Signed-in sessions end rather than lingering on a shared machine. */
$cfg['LoginCookieValidity'] = 1800;
$cfg['LoginCookieStore'] = 0;

/* The version-check call to phpmyadmin.net is off: a panel-managed host
   should not make outbound requests nobody asked for, and the panel is what
   updates the package. */
$cfg['VersionCheck'] = false;

/*
 * Where phpMyAdmin believes it lives.
 *
 * The panel proxies it under its own origin at this path, and that is not a
 * cosmetic choice. phpMyAdmin's login is a POST carrying a CSRF token bound to
 * a session cookie it set on the page the form came from, so signing somebody
 * in requires reading that page first — which only same-origin JavaScript can
 * do. Off its own hostname, "open this database" could never be more than a
 * login form with the username typed in.
 *
 * Without this, every redirect phpMyAdmin issues drops the prefix and lands on
 * the panel's own router instead.
 */
$cfg['PmaAbsoluteUri'] = '{{ .BaseURI }}';

/* Suggestions to change settings the panel manages are noise here. */
$cfg['ShowServerInfo'] = false;
$cfg['ShowPhpInfo'] = false;
$cfg['ShowChgPassword'] = true;
`))

// configData is what the template renders.
type configData struct {
	Secret  string
	Socket  string
	TempDir string
	BaseURI string
}

// BaseURI is the path the panel proxies phpMyAdmin at, on the panel's own
// origin. It appears here, in the panel's nginx configuration and in the
// frontend; those three have to agree, and this is the one they are checked
// against.
const BaseURI = "/phpmyadmin/"


// renderConfig produces config.inc.php.
func renderConfig(secret string) string {
	var out strings.Builder
	// The template is a compile-time constant and the data is generated here,
	// so this cannot fail for a reason a caller could act on.
	_ = configTemplate.Execute(&out, configData{
		Secret: secret,
		// The local socket rather than TCP: it is what the Agent itself uses,
		// it needs no listening port, and it keeps working on a server with
		// skip-networking set.
		Socket:  defaultMySQLSocket,
		TempDir: TempDir,
		BaseURI: BaseURI,
	})
	return out.String()
}

// defaultMySQLSocket is where MariaDB and MySQL put their socket on the
// distributions this panel supports.
const defaultMySQLSocket = "/run/mysqld/mysqld.sock"

<?php
declare(strict_types=1);

// Read the same daemon objects as occ app_api:daemon:list. AppAPI 33 does not
// expose its JSON output option. Emit only the fields asserted by the fixture,
// avoiding credentials in deploy_config.
define('OC_CONSOLE', 1);
require '/var/www/html/lib/base.php';
\OC_App::loadApp('app_api');
$service = \OCP\Server::get(\OCA\AppAPI\Service\DaemonConfigService::class);
$daemons = [];
foreach ($service->getRegisteredDaemonConfigs() as $daemon) {
    $daemons[] = [
        'name' => $daemon->getName(),
        'deploy_id' => $daemon->getAcceptsDeployId(),
        'harp' => isset($daemon->getDeployConfig()['harp']),
    ];
}
echo json_encode($daemons, JSON_THROW_ON_ERROR) . PHP_EOL;

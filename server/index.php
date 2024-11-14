<?php
//usleep(10 * 1000);
//sleep(1);
// for ($i = 0; $i < 10000; $i++) {
//     echo '.';
// }
//
// to test out of order
$address = explode('/', $_SERVER['PATH_INFO'])[1];
if ($_SERVER['REQUEST_METHOD'] == 'GET') {
    echo "ok";
} else {
    //if (explode(':', $_GET['addr'])[1] % 4 == 0) usleep(random_int(0, 100) * 10000);
    echo json_encode(sprintf("I got '%s' via '%s' from '%s'", $HTTP_RAW_POST_DATA, $_SERVER['REMOTE_ADDR'] . ':4000', $address));
    // test remote initiated message
    if (random_int(0, 100) == 0) {
        $ch = curl_init();
        curl_setopt($ch, CURLOPT_URL, "http://" . $_SERVER['REMOTE_ADDR'] . ':4000/' . $address);
        $payload = json_encode([2, "123", "action", ["call-from-php" => true]]);
        curl_setopt($ch, CURLOPT_POSTFIELDS, $payload);
        curl_setopt($ch, CURLOPT_RETURNTRANSFER, 1);
        curl_exec($ch);
        curl_close($ch);
    }
}

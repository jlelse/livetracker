document.addEventListener('DOMContentLoaded', () => {
    const map = L.map('map').setView([51.505, -0.09], 13);
    L.tileLayer('https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png', {
        maxZoom: 19,
        attribution: '© OpenStreetMap contributors'
    }).addTo(map);

    const statusEl = document.getElementById('status');
    const lastUpdateEl = document.getElementById('lastUpdate');
    const coordsEl = document.getElementById('coords');
    const speedEl = document.getElementById('speed');

    let currentMarker = null;
    let accuracyCircle = null;
    let trackPolyline = L.polyline([], { color: 'blue' }).addTo(map);
    let polylinePoints = [];
    let timestampMarkers = [];
    let ws;

    trackPolyline.on('click', function(e) {
        if (polylinePoints.length === 0) return;
        let minDist = Infinity;
        let closestIdx = 0;
        for (let i = 0; i < polylinePoints.length; i++) {
            const latlng = L.latLng(polylinePoints[i].lat, polylinePoints[i].lon);
            const dist = e.latlng.distanceTo(latlng);
            if (dist < minDist) {
                minDist = dist;
                closestIdx = i;
            }
        }
        timestampMarkers.forEach(m => map.removeLayer(m));
        timestampMarkers = [];
        const point = polylinePoints[closestIdx];
        const marker = L.marker([point.lat, point.lon]).addTo(map)
            .bindPopup(new Date(point.timestamp).toLocaleString())
            .openPopup();
        timestampMarkers.push(marker);
    });

    function connectWebSocket() {
        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        ws = new WebSocket(`${protocol}//${window.location.host}/ws`);

        ws.onopen = () => {
            statusEl.textContent = 'Connected';
            console.log('WebSocket connected');
            ws.send(JSON.stringify({ type: 'get_history' }));
        };

        ws.onmessage = (event) => {
            try {
                const data = JSON.parse(event.data);
                if (data.type === 'update') {
                    handleLocationUpdate(data.payload);
                } else if (data.type === 'history' && data.payload.length > 0) {
                    handleHistory(data.payload);
                } else if (data.type === 'history' && data.payload.length === 0) {
                    console.log('No historical data received');
                    statusEl.textContent = 'Connected (no history)';
                }
            } catch (e) {
                console.error('Error parsing WebSocket message:', e);
            }
        };

        ws.onclose = () => {
            statusEl.textContent = 'Disconnected. Reconnecting in 5s...';
            console.log('WebSocket disconnected. Reconnecting in 5 seconds...');
            setTimeout(connectWebSocket, 5000);
        };

        ws.onerror = (error) => {
            statusEl.textContent = 'WebSocket error';
            console.error('WebSocket error:', error);
            ws.close();
        };
    }

    function handleLocationUpdate(point) {
        const latLng = [point.lat, point.lon];
        console.log('Live update:', point);

        if (!currentMarker) {
            currentMarker = L.marker(latLng).addTo(map)
                .bindPopup('Current Position')
                .openPopup();
            map.setView(latLng, 16);
        } else {
            currentMarker.setLatLng(latLng);
        }
        if (point.hdop) {
            if (!accuracyCircle) {
                accuracyCircle = L.circle(latLng, {
                    radius: point.hdop,
                    color: 'blue',
                    fillColor: '#3fa9f5',
                    fillOpacity: 0.2,
                    weight: 1
                }).addTo(map);
            } else {
                accuracyCircle.setLatLng(latLng);
                accuracyCircle.setRadius(point.hdop);
            }
        } else if (accuracyCircle) {
            map.removeLayer(accuracyCircle);
            accuracyCircle = null;
        }
        trackPolyline.addLatLng(latLng);
        polylinePoints.push(point);
        if (document.hidden) {
            map.panTo(latLng);
        }

        lastUpdateEl.textContent = new Date(point.timestamp).toLocaleString();
        coordsEl.textContent = `${point.lat.toFixed(5)}, ${point.lon.toFixed(5)}`;
        if (point.speed !== null && typeof point.speed !== 'undefined') {
            speedEl.textContent = (point.speed * 3.6).toFixed(1); // m/s to km/h
        } else {
            speedEl.textContent = '-';
        }
    }

    function handleHistory(points) {
        console.log(`Received ${points.length} historical points`);
        const latLngs = points.map(p => [p.lat, p.lon]);
        trackPolyline.setLatLngs(latLngs);
        polylinePoints = points.slice(0, -1);

        if (points.length > 0) {
            const lastPoint = points[points.length - 1];
            handleLocationUpdate(lastPoint);
            map.fitBounds(trackPolyline.getBounds(), { padding: [50, 50] });
        } else {
            statusEl.textContent = 'Connected (no history)';
        }
    }

    connectWebSocket();
});
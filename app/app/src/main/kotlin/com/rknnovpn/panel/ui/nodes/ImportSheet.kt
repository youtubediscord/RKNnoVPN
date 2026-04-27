package com.rknnovpn.panel.ui.nodes

import android.Manifest
import android.content.pm.PackageManager
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CameraAlt
import androidx.compose.material.icons.filled.ContentPaste
import androidx.compose.material.icons.filled.Link
import androidx.compose.material3.Button
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Tab
import androidx.compose.material3.TabRow
import androidx.compose.material3.Text
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.camera.core.CameraSelector
import androidx.camera.core.ExperimentalGetImage
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.ImageProxy
import androidx.camera.core.Preview
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.core.content.ContextCompat
import androidx.lifecycle.LifecycleOwner
import com.rknnovpn.panel.R
import com.google.mlkit.vision.barcode.BarcodeScanner
import com.google.mlkit.vision.barcode.BarcodeScanning
import com.google.mlkit.vision.common.InputImage
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicReference

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ImportSheet(
    initialTab: ImportSheetTab,
    initialText: String,
    candidates: List<ImportCandidate>,
    canApplyEmptySubscriptionPreview: Boolean,
    isLoading: Boolean,
    errorMessage: String?,
    statusMessage: String?,
    onDetectUris: (String) -> Unit,
    onToggleCandidate: (Int) -> Unit,
    onImportSelected: () -> Unit,
    onFetchSubscription: (String) -> Unit,
    onDismiss: () -> Unit,
) {
    val sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)
    var selectedTab by remember(initialTab) { mutableStateOf(initialTab) }

    ModalBottomSheet(
        onDismissRequest = onDismiss,
        sheetState = sheetState,
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 16.dp)
                .padding(bottom = 24.dp),
        ) {
            Text(
                text = stringResource(R.string.import_nodes),
                style = MaterialTheme.typography.titleLarge,
                modifier = Modifier.padding(bottom = 16.dp),
            )

            TabRow(selectedTabIndex = selectedTab.ordinal) {
                Tab(
                    selected = selectedTab == ImportSheetTab.PASTE_URI,
                    onClick = { selectedTab = ImportSheetTab.PASTE_URI },
                    text = { Text(stringResource(R.string.tab_paste_uri)) },
                    icon = {
                        Icon(
                            Icons.Filled.ContentPaste,
                            contentDescription = null,
                            modifier = Modifier.size(18.dp),
                        )
                    },
                )
                Tab(
                    selected = selectedTab == ImportSheetTab.SCAN_QR,
                    onClick = { selectedTab = ImportSheetTab.SCAN_QR },
                    text = { Text(stringResource(R.string.tab_scan_qr)) },
                    icon = {
                        Icon(
                            Icons.Filled.CameraAlt,
                            contentDescription = null,
                            modifier = Modifier.size(18.dp),
                        )
                    },
                )
                Tab(
                    selected = selectedTab == ImportSheetTab.SUBSCRIPTION,
                    onClick = { selectedTab = ImportSheetTab.SUBSCRIPTION },
                    text = { Text(stringResource(R.string.tab_subscription)) },
                    icon = {
                        Icon(
                            Icons.Filled.Link,
                            contentDescription = null,
                            modifier = Modifier.size(18.dp),
                        )
                    },
                )
            }

            Spacer(modifier = Modifier.height(16.dp))

            when (selectedTab) {
                ImportSheetTab.PASTE_URI -> PasteUriTab(
                    initialText = if (initialTab == ImportSheetTab.PASTE_URI) initialText else "",
                    onDetectUris = onDetectUris,
                )
                ImportSheetTab.SCAN_QR -> ScanQrTab(onQrDetected = onDetectUris)
                ImportSheetTab.SUBSCRIPTION -> SubscriptionTab(
                    initialUrl = if (initialTab == ImportSheetTab.SUBSCRIPTION) initialText else "",
                    onFetchSubscription = onFetchSubscription,
                )
            }

            if (errorMessage != null) {
                Spacer(modifier = Modifier.height(12.dp))
                Text(
                    text = errorMessage,
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.error,
                    modifier = Modifier.fillMaxWidth(),
                )
            }

            if (errorMessage == null && statusMessage != null) {
                Spacer(modifier = Modifier.height(12.dp))
                Text(
                    text = statusMessage,
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.primary,
                    modifier = Modifier.fillMaxWidth(),
                )
            }

            if (isLoading) {
                Spacer(modifier = Modifier.height(12.dp))
                Row(
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.Center,
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    CircularProgressIndicator(modifier = Modifier.size(24.dp))
                    Spacer(modifier = Modifier.size(8.dp))
                    Text(
                        text = stringResource(R.string.importing),
                        style = MaterialTheme.typography.bodyMedium,
                    )
                }
            }

            if (candidates.isNotEmpty()) {
                Spacer(modifier = Modifier.height(16.dp))
                Text(
                    text = stringResource(R.string.nodes_detected, candidates.size),
                    style = MaterialTheme.typography.labelLarge,
                )
                Spacer(modifier = Modifier.height(8.dp))
                LazyColumn(
                    modifier = Modifier.heightIn(max = 240.dp),
                    verticalArrangement = Arrangement.spacedBy(4.dp),
                ) {
                    itemsIndexed(candidates) { index, candidate ->
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            modifier = Modifier
                                .fillMaxWidth()
                                .padding(vertical = 4.dp),
                        ) {
                            Checkbox(
                                checked = candidate.selected,
                                enabled = candidate.selectable,
                                onCheckedChange = { onToggleCandidate(index) },
                            )
                            Column(modifier = Modifier.weight(1f)) {
                                Text(
                                    text = candidate.node.name,
                                    style = MaterialTheme.typography.bodyMedium,
                                    fontWeight = FontWeight.Medium,
                                )
                                Text(
                                    text = "${candidate.node.protocol.name} | ${candidate.node.server}:${candidate.node.port}",
                                    style = MaterialTheme.typography.bodySmall,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis,
                                )
                            }
                        }
                    }
                }
                Spacer(modifier = Modifier.height(12.dp))
                val selectedCount = candidates.count { it.selected }
                val subscriptionPreview = candidates.any { !it.selectable }
                Button(
                    onClick = onImportSelected,
                    enabled = selectedCount > 0 && !isLoading,
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Text(
                        if (subscriptionPreview) {
                            stringResource(R.string.apply_subscription_preview)
                        } else {
                            stringResource(R.string.import_selected, selectedCount)
                        }
                    )
                }
            } else if (canApplyEmptySubscriptionPreview) {
                Spacer(modifier = Modifier.height(16.dp))
                Button(
                    onClick = onImportSelected,
                    enabled = !isLoading,
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Text(stringResource(R.string.apply_subscription_preview))
                }
            }
        }
    }
}

@Composable
private fun PasteUriTab(
    initialText: String,
    onDetectUris: (String) -> Unit,
) {
    val clipboardManager = LocalClipboardManager.current
    var text by remember(initialText) { mutableStateOf(initialText) }

    OutlinedTextField(
        value = text,
        onValueChange = { text = it },
        label = { Text(stringResource(R.string.paste_uri_hint)) },
        modifier = Modifier
            .fillMaxWidth()
            .heightIn(min = 120.dp),
        maxLines = 10,
    )

    Spacer(modifier = Modifier.height(12.dp))

    Row(
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        modifier = Modifier.fillMaxWidth(),
    ) {
        OutlinedButton(
            onClick = {
                val clipboardText = clipboardManager.getText()?.text.orEmpty()
                if (clipboardText.isNotBlank()) {
                    text = clipboardText
                    onDetectUris(clipboardText)
                }
            },
            modifier = Modifier.weight(1f),
        ) {
            Icon(
                Icons.Filled.ContentPaste,
                contentDescription = null,
                modifier = Modifier.size(18.dp),
            )
            Spacer(modifier = Modifier.size(8.dp))
            Text(
                text = stringResource(R.string.paste_from_clipboard),
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        }

        FilledTonalButton(
            onClick = { onDetectUris(text) },
            enabled = text.isNotBlank(),
            modifier = Modifier.weight(1f),
        ) {
            Text(
                text = stringResource(R.string.detect_uris),
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        }
    }
}

@Composable
private fun ScanQrTab(
    onQrDetected: (String) -> Unit,
) {
    val context = LocalContext.current
    var hasPermission by remember {
        mutableStateOf(
            ContextCompat.checkSelfPermission(context, Manifest.permission.CAMERA) ==
                PackageManager.PERMISSION_GRANTED
        )
    }
    val permissionLauncher = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.RequestPermission(),
    ) { granted ->
        hasPermission = granted
    }

    if (hasPermission) {
        QrScannerPreview(onQrDetected = onQrDetected)
    } else {
        Box(
            contentAlignment = Alignment.Center,
            modifier = Modifier
                .fillMaxWidth()
                .height(240.dp),
        ) {
            Column(horizontalAlignment = Alignment.CenterHorizontally) {
                Icon(
                    Icons.Filled.CameraAlt,
                    contentDescription = null,
                    modifier = Modifier.size(64.dp),
                    tint = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                Spacer(modifier = Modifier.height(8.dp))
                FilledTonalButton(onClick = { permissionLauncher.launch(Manifest.permission.CAMERA) }) {
                    Text(stringResource(R.string.camera_permission_button))
                }
            }
        }
    }
}

@androidx.annotation.OptIn(ExperimentalGetImage::class)
@Composable
	private fun QrScannerPreview(
	    onQrDetected: (String) -> Unit,
	) {
	    val lifecycleOwner = LocalContext.current as LifecycleOwner
    val executor = remember { Executors.newSingleThreadExecutor() }
    val scanner = remember { BarcodeScanning.getClient() }
    val lastValue = remember { AtomicReference<String?>(null) }
    val cameraProviderRef = remember { AtomicReference<ProcessCameraProvider?>(null) }

    DisposableEffect(Unit) {
        onDispose {
            cameraProviderRef.get()?.unbindAll()
            scanner.close()
            executor.shutdown()
        }
    }

    AndroidView(
        modifier = Modifier
            .fillMaxWidth()
            .height(280.dp),
        factory = { context ->
            val previewView = PreviewView(context).apply {
                scaleType = PreviewView.ScaleType.FILL_CENTER
            }
            val cameraProviderFuture = ProcessCameraProvider.getInstance(context)
            cameraProviderFuture.addListener(
                {
                    val cameraProvider = cameraProviderFuture.get()
                    cameraProviderRef.set(cameraProvider)
                    val preview = Preview.Builder().build().also {
                        it.setSurfaceProvider(previewView.surfaceProvider)
                    }
                    val analysis = ImageAnalysis.Builder()
                        .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
                        .build()

                    analysis.setAnalyzer(executor) { imageProxy ->
                        scanBarcode(
                            imageProxy = imageProxy,
                            scanner = scanner,
                            onValue = { value ->
                                if (value.isNotBlank() && value != lastValue.getAndSet(value)) {
                                    previewView.post { onQrDetected(value) }
                                }
                            },
                        )
                    }

                    runCatching {
                        cameraProvider.unbindAll()
                        cameraProvider.bindToLifecycle(
                            lifecycleOwner,
                            CameraSelector.DEFAULT_BACK_CAMERA,
                            preview,
                            analysis,
                        )
                    }
                },
                ContextCompat.getMainExecutor(context),
            )
            previewView
        },
    )
}

@androidx.annotation.OptIn(ExperimentalGetImage::class)
private fun scanBarcode(
    imageProxy: ImageProxy,
    scanner: BarcodeScanner,
    onValue: (String) -> Unit,
) {
    val mediaImage = imageProxy.image
    if (mediaImage == null) {
        imageProxy.close()
        return
    }
    val image = InputImage.fromMediaImage(mediaImage, imageProxy.imageInfo.rotationDegrees)
    scanner.process(image)
        .addOnSuccessListener { barcodes ->
            barcodes.firstNotNullOfOrNull { it.rawValue }?.let(onValue)
        }
        .addOnCompleteListener {
            imageProxy.close()
        }
}

@Composable
private fun SubscriptionTab(
    initialUrl: String,
    onFetchSubscription: (String) -> Unit,
) {
    var url by remember(initialUrl) { mutableStateOf(initialUrl) }

    OutlinedTextField(
        value = url,
        onValueChange = { url = it },
        label = { Text(stringResource(R.string.subscription_url_hint)) },
        singleLine = true,
        modifier = Modifier.fillMaxWidth(),
    )

    Spacer(modifier = Modifier.height(12.dp))

    FilledTonalButton(
        onClick = { onFetchSubscription(url) },
        enabled = url.isNotBlank(),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Text(stringResource(R.string.fetch))
    }
}

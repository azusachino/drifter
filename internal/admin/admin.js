document.addEventListener('submit', async (event) => {
  const form = event.target;
  if (!(form instanceof HTMLFormElement) || !form.matches('form[data-mapping-action]')) return;
  event.preventDefault();

  const fields = new FormData(form);
  const action = form.dataset.mappingAction;
  const token = fields.get('csrf_token');
  const status = document.querySelector('#mutation-status');
  const statusMessage = (message) => {
    status.textContent = message;
    status.focus();
  };
  let endpoint;
  let method;
  let payload;
  if (action === 'confirm' || action === 'replace') {
    endpoint = `/api/mappings/${action}`;
    method = 'POST';
    const workID = Number(fields.get('work_id'));
    const editionMode = fields.get('edition_mode');
    const rawEditionID = fields.get('edition_id');
    let editionID = 0;
    if (!Number.isSafeInteger(workID) || workID < 1) {
      statusMessage('Enter a valid target work ID.');
      return;
    }
    if (editionMode === 'edition') {
      editionID = Number(rawEditionID);
      if (!rawEditionID || !Number.isSafeInteger(editionID) || editionID < 1) {
        statusMessage('Enter an explicit target edition ID.');
        return;
      }
    } else if (editionMode !== 'work_only') {
      statusMessage('Choose work only or a specific edition.');
      return;
    }
    payload = {
      source_account: fields.get('source_account'),
      book_id: fields.get('book_id'),
      target_account: fields.get('target_account'),
      work_id: workID,
      edition_id: editionID,
    };
  } else if (action === 'unlink') {
    endpoint = '/api/mappings/unlink';
    method = 'DELETE';
    payload = {
      source_account: fields.get('source_account'),
      book_id: fields.get('book_id'),
      target_account: fields.get('target_account'),
    };
  } else {
    return;
  }

  status.textContent = 'Saving mapping…';
  try {
    const response = await fetch(endpoint, {
      method,
      headers: {
        'Content-Type': 'application/json',
        'X-Drifter-CSRF': String(token),
      },
      body: JSON.stringify(payload),
      credentials: 'same-origin',
    });
    if (response.ok) {
      const notice = action === 'confirm' ? 'confirmed' : action === 'replace' ? 'replaced' : 'unlinked';
      window.location.assign(`/admin?notice=${notice}`);
      return;
    }
    const result = await response.json();
    const messages = {
      invalid_mapping: 'The mapping selection is invalid.',
      mapping_conflict: 'A mapping already exists; choose Replace for an explicit change.',
      mapping_validation_failed: 'The source or target selection could not be validated.',
      mapping_database_unavailable: 'Mapping storage is unavailable.',
      provider_unavailable: 'Provider configuration is unavailable.',
      request_rejected: 'The request was rejected. Reload the page and try again.',
    };
    status.textContent = messages[result.error] || 'The mapping request failed.';
    status.focus();
  } catch {
    status.textContent = 'The mapping request failed. Check the local server and try again.';
    status.focus();
  }
});
